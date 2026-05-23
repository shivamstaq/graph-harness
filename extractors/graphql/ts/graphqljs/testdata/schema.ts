import { gql } from 'graphql-tag';

export const typeDefs = gql`
  type Query {
    user(id: ID!): User
    users: [User!]!
  }

  type Mutation {
    createUser(name: String!, age: Int): User!
  }

  type Subscription {
    userAdded: User
  }
`;

export const resolvers = {
  Query: {
    user: (_, { id }) => findUser(id),
    users: () => listUsers(),
  },
  Mutation: {
    createUser: async (_, { name, age }) => createUser(name, age),
  },
  Subscription: {
    userAdded: {
      subscribe: () => pubsub.asyncIterator(['USER_ADDED']),
    },
  },
};
